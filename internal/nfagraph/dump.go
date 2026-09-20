package nfagraph

import (
	"encoding/json"
	"fmt"

	"github.com/smartwalle/scankit/internal/graph"
)

const Version = 1

func CurrentVersion() int { return Version }

type dumpGraph struct {
	Version int      `json:"version"`
	Start   int      `json:"start"`
	Nodes   []Node   `json:"nodes"`
	Edges   [][2]int `json:"edges"`
}

func Marshal(g *Graph) ([]byte, error)      { return Dump(g) }
func Unmarshal(data []byte) (*Graph, error) { return Load(data) }

// Dump 返回稳定的 JSON 表示，便于诊断和固定样例测试。
func Dump(g *Graph) ([]byte, error) {
	if err := g.Validate(); err != nil {
		return nil, err
	}
	d := dumpGraph{Version: Version, Start: int(g.Start)}
	for _, id := range g.Flow.Vertices() {
		d.Nodes = append(d.Nodes, *g.Nodes[id])
		for _, to := range g.Flow.Successors(id) {
			d.Edges = append(d.Edges, [2]int{int(id), int(to)})
		}
	}
	return json.Marshal(d)
}

// Load 解析 Dump 生成的数据并校验结构。
func Load(data []byte) (*Graph, error) {
	var d dumpGraph
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, err
	}
	if d.Version != Version {
		return nil, fmt.Errorf("unsupported graph version %d", d.Version)
	}
	g := &Graph{Flow: graph.NewDirected(), Nodes: map[graph.Vertex]*Node{}, Start: graph.Vertex(d.Start)}
	seenNodes := make(map[graph.Vertex]struct{}, len(d.Nodes))
	for _, n := range d.Nodes {
		if _, exists := seenNodes[n.ID]; exists {
			return nil, fmt.Errorf("duplicate graph node %d", n.ID)
		}
		seenNodes[n.ID] = struct{}{}
		g.AddNode(n)
	}
	seenEdges := make(map[[2]graph.Vertex]struct{}, len(d.Edges))
	for _, e := range d.Edges {
		edge := [2]graph.Vertex{graph.Vertex(e[0]), graph.Vertex(e[1])}
		if _, exists := seenEdges[edge]; exists {
			return nil, fmt.Errorf("duplicate graph edge")
		}
		seenEdges[edge] = struct{}{}
		g.AddEdge(edge[0], edge[1])
	}
	if err := g.Validate(); err != nil {
		return nil, err
	}
	return g, nil
}
