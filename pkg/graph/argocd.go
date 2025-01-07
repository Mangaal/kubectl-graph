package graph

// ArgoCDGraph is used to graph all ArgoCD resources.
type ArgoCDGraph struct {
	graph *Graph
}

// NewArgoCDGraph creates a new ArgoCDGraph.
func NewArgoCDGraph(g *Graph) *ArgoCDGraph {
	return &ArgoCDGraph{
		graph: g,
	}
}

// RouteV1 retrieves the RouteV1Graph.
func (g *Graph) ArgoCDGraph() *ArgoCDGraph {
	return g.argocd
}
