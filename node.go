package influxdb

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const nodeFile = "node.json"

type Node struct {
	path        string
	ID          uint64
	MetaServers []string
}

// NewNode will load the node information from disk if present
func NewNode(path string) (*Node, error) {
	n := &Node{
		path: path,
	}

	f, err := os.Open(filepath.Join(path, nodeFile))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	defer f.Close()

	if err := json.NewDecoder(f).Decode(n); err != nil {
		return nil, err
	}

	return n, nil
}

// Save will save the node file to disk and replace the existing one if present
func (n *Node) Save() error {
	tmpFile := filepath.Join(n.path, nodeFile, "tmp")

	f, err := os.Open(tmpFile)
	if err != nil {
		return err
	}
	defer f.Close()

	return json.NewEncoder(f).Encode(n)
}
