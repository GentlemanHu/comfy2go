package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/GentlemanHu/comfy2go/graphapi"
)

/*
@routes.get("/embeddings")
@routes.get("/extensions")
@routes.get("/view")
@routes.get("/view_metadata/{folder_name}")
@routes.get("/system_stats")
@routes.get("/prompt")
@routes.get("/object_info")
@routes.get("/object_info/{node_class}")
@routes.get("/history")
@routes.get("/history/{prompt_id}")
@routes.get("/queue")

@routes.post("/prompt")
@routes.post("/queue")
@routes.post("/interrupt")
@routes.post("/history")
@routes.post("/upload/image")
@routes.post("/upload/mask")
*/

func (c *ComfyClient) makeRequest(method string, path string, header http.Header, body io.Reader) (*http.Response, error) {
	// Create a new request
	req, err := http.NewRequest(method, fmt.Sprintf("%s/%s", c.serverBaseAddress, path), body)
	if err != nil {
		return nil, err
	}

	// Add default Authorization header if needed
	if c.authHeader != "" {
		req.Header.Add("Authorization", c.authHeader)
	}

	// Add additional headers if provided
	if header != nil {
		for key, values := range header {
			for _, value := range values {
				req.Header.Add(key, value)
			}
		}
	}

	// Create a client and execute the request
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}

	return resp, nil
}

func (c *ComfyClient) GetSystemStats() (*SystemStats, error) {
	err := c.CheckConnection()
	if err != nil {
		return nil, err
	}

	resp, err := c.makeRequest("GET", "system_stats", nil, nil)
	if err != nil {
		return nil, err
	}

	body, _ := io.ReadAll(resp.Body)
	retv := &SystemStats{}
	err = json.Unmarshal(body, &retv)
	if err != nil {
		return nil, err
	}

	return retv, nil
}

func (c *ComfyClient) GetPromptHistoryByIndex() ([]PromptHistoryItem, error) {
	history, err := c.GetPromptHistoryByID()
	if err != nil {
		return nil, err
	}

	retv := make([]PromptHistoryItem, len(history))
	index := 0
	// ComfyUI does not recalculate the indicies of prompt history items,
	// so the indecies may not always be ordered 0..n
	// We'll create a slice out of the map items, and then sort them
	for _, h := range history {
		retv[index] = h
		index++
	}

	sort.Slice(retv, func(i, j int) bool {
		return retv[i].Index < retv[j].Index
	})

	return retv, nil
}

func (c *ComfyClient) GetPromptHistoryByID() (map[string]PromptHistoryItem, error) {
	resp, err := c.makeRequest("GET", "history", nil, nil)
	if err != nil {
		return nil, err
	}

	// we need to re-arrange the data into something more coherent
	// We're going to have to make an adapter that reconstructs an actual prompt
	// from the mangled data
	type internalOutputs struct {
		Images *[]DataOutput `json:"images"`
	}
	type internalPromptHistoryItem struct {
		// The prompt is stored as an array layed out like this:
		// [
		// 	[0] index 		int,
		// 	[1] promptID 	string,
		// 	[2] prompt 		map[string]graphapi.PromptNode, // we'll ignore this
		// 	[3] extra_data 	graphapi.PromptExtraData,       // the graph is in here
		//  [4] outputs     []string 						// array of nodeIDs that have outputs
		// ]
		Prompt  []interface{}              `json:"prompt"`
		Outputs map[string]internalOutputs `json:"outputs"`
	}

	// read in the body, and deserialize to our temp internalPromptHistoryItem type
	body, _ := io.ReadAll(resp.Body)
	history := make(map[string]internalPromptHistoryItem)
	err = json.Unmarshal(body, &history)
	if err != nil {
		return nil, err
	}

	// try to reconstruct the data into PromptHistoryItem
	ret := make(map[string]PromptHistoryItem)
	for k, ph := range history {
		index := int(ph.Prompt[0].(float64))

		// extract the graph from ph.Prompt[3]["extra_pnginfo"]["workflow"]
		extra_data, _ := ph.Prompt[3].(map[string]interface{})
		extra_pnginfo, _ := extra_data["extra_pnginfo"].(map[string]interface{})
		workflow := extra_pnginfo["workflow"]
		// workflow is now an interface{}
		// serialize it back and re-deserialize as a graph
		// this could be more efficient with raw json, but ugh!
		gdata, _ := json.Marshal(workflow)
		graph := &graphapi.Graph{}
		err = json.Unmarshal(gdata, &graph)
		if err != nil {
			return nil, err
		}

		// reconstruct
		item := &PromptHistoryItem{
			PromptID: k,
			Index:    index,
			Graph:    graph,
			Outputs:  make(map[int][]DataOutput),
		}

		// rebuild the images output map
		for k, o := range ph.Outputs {
			oid, _ := strconv.Atoi(k)
			item.Outputs[oid] = *o.Images
		}
		ret[k] = *item
	}
	return ret, nil
}

// GetViewMetadata retrieves the '__metadata__' field in a safetensors file.
// checkpoints
// vae
// loras
// clip
// unet
// controlnet
// style_models
// clip_vision
// gligen
// configs
// hypernetworks
// upscale_models
// onnx
// fonts
func (c *ComfyClient) GetViewMetadata(folder string, file string) (string, error) {
	path := fmt.Sprintf("view_metadata/%s?filename=%s", folder, file)
	resp, err := c.makeRequest("GET", path, nil, nil)
	if err != nil {
		return "", err
	}

	body, _ := io.ReadAll(resp.Body)
	return string(body), nil
}

// GetImage
func (c *ComfyClient) GetImage(image_data DataOutput) (*[]byte, error) {
	u, err := url.Parse(fmt.Sprintf("%s/view", c.serverBaseAddress))
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("filename", image_data.Filename)
	q.Set("subfolder", image_data.Subfolder)
	q.Set("type", image_data.Type)
	u.RawQuery = q.Encode()

	// Make the request
	resp, err := c.makeRequest("GET", u.String(), nil, nil)

	if err != nil {
		return nil, err
	}

	body, _ := io.ReadAll(resp.Body)
	return &body, nil
}

// GetEmbeddings retrieves the list of Embeddings models installed on the ComfyUI server.
func (c *ComfyClient) GetEmbeddings() ([]string, error) {
	resp, err := c.makeRequest("GET", "embeddings", nil, nil)
	if err != nil {
		return nil, err
	}

	body, _ := io.ReadAll(resp.Body)
	retv := make([]string, 0)
	err = json.Unmarshal(body, &retv)
	if err != nil {
		return nil, err
	}

	return retv, nil
}

func (c *ComfyClient) GetQueueExecutionInfo() (*QueueExecInfo, error) {
	resp, err := c.makeRequest("GET", "prompt", nil, nil)
	if err != nil {
		return nil, err
	}

	body, _ := io.ReadAll(resp.Body)
	queue_exec := &QueueExecInfo{}
	err = json.Unmarshal(body, &queue_exec)
	if err != nil {
		return nil, err
	}

	return queue_exec, nil
}

// GetExtensions retrieves the list of extensions installed on the ComfyUI server.
func (c *ComfyClient) GetExtensions() ([]string, error) {
	resp, err := c.makeRequest("GET", "extensions", nil, nil)
	if err != nil {
		return nil, err
	}

	body, _ := io.ReadAll(resp.Body)
	retv := make([]string, 0)
	err = json.Unmarshal(body, &retv)
	if err != nil {
		return nil, err
	}

	return retv, nil
}

func (c *ComfyClient) GetObjectInfos() (*graphapi.NodeObjects, error) {
	resp, err := c.makeRequest("GET", "object_info", nil, nil)

	if err != nil {
		return nil, err
	}

	body, _ := io.ReadAll(resp.Body)
	result := &graphapi.NodeObjects{}
	err = json.Unmarshal(body, &result.Objects)
	if err != nil {
		return nil, err
	}

	result.PopulateInputProperties()
	return result, nil
}

func (c *ComfyClient) QueuePrompt(graph *graphapi.Graph) (*QueueItem, error) {
	err := c.CheckConnection()
	if err != nil {
		return nil, err
	}

	prompt, err := graph.GraphToPrompt(c.clientid)
	if err != nil {
		return nil, err
	}

	// Prevent a race where the ws may provide messages about a queued item before
	// we add the item to our internal map
	c.webSocket.LockRead()
	defer c.webSocket.UnlockRead()

	data, _ := json.Marshal(prompt)

	// Make the POST request using makeRequest
	header := http.Header{}
	header.Add("Content-Type", "application/json") // Set Content-Type header
	resp, err := c.makeRequest("POST", "prompt", header, strings.NewReader(string(data)))

	if err != nil {
		return nil, err
	}

	body, _ := io.ReadAll(resp.Body)

	// create the queue item
	item := &QueueItem{
		Workflow: graph,
		Messages: make(chan PromptMessage),
	}

	err = json.Unmarshal(body, &item)
	if err != nil {
		// mmm-k, is it one of these:
		// {"error": {"type": "prompt_no_outputs",
		//				"message": "Prompt has no outputs",
		//				"details": "",
		//				"extra_info": {}
		//			  },
		// "node_errors": []
		// }
		perror := &PromptErrorMessage{}
		perr := json.Unmarshal(body, &perror)
		if perr != nil {
			// return the original error
			slog.Error("error unmarshalling prompt error", "body", string(body))
			return nil, err
		} else {
			return nil, errors.New(perror.Error.Message)
		}
	}
	c.queueditems[item.PromptID] = item
	return item, nil
}

func (c *ComfyClient) Interrupt() error {
	// Make the POST request using makeRequest
	header := http.Header{}
	header.Add("Content-Type", "application/json")
	resp, err := c.makeRequest("POST", "interrupt", header, strings.NewReader("{}"))
	if err != nil {
		return err
	}

	io.ReadAll(resp.Body)
	return nil
}

func (c *ComfyClient) EraseHistory() error {
	// Create the data
	data := "{\"clear\": \"clear\"}"

	// Make the POST request using makeRequest
	header := http.Header{}
	header.Add("Content-Type", "application/json")
	resp, err := c.makeRequest("POST", "history", header, strings.NewReader(data))
	if err != nil {
		return err
	}

	io.ReadAll(resp.Body)
	return nil
}

func (c *ComfyClient) EraseHistoryItem(promptID string) error {
	// Create the data
	item := fmt.Sprintf("{\"delete\": [\"%s\"]}", promptID)

	// Make the POST request using makeRequest
	header := http.Header{}
	header.Add("Content-Type", "application/json")
	resp, err := c.makeRequest("POST", "history", header, strings.NewReader(item))
	if err != nil {
		return err
	}

	io.ReadAll(resp.Body)
	return nil
}
