package graph

import (
	"context"
	"fmt"
	"strings"
	"sync"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

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

// Unstructured adds an unstructured node to the Graph.
func (g *ArgoCDGraph) Unstructured(unstr *unstructured.Unstructured) (*Node, error) {
	switch unstr.GetKind() {
	case "ApplicationSet":
		return g.ApplicationSet(unstr)
	case "Application":
		return g.Application(unstr)
	case "AppProject":
		return g.AppProject(unstr)
	default:
		return g.graph.Node(unstr.GroupVersionKind(), unstr), nil
	}
}

// Application adds a v1alpha1.Application resource to the Graph.
func (g *ArgoCDGraph) Application(app *unstructured.Unstructured) (*Node, error) {
	n := g.graph.Node(app.GroupVersionKind(), app)
	fields := app.Object
	projName := fields["spec"].(map[string]interface{})["project"].(string)
	//destinationNamespace := fields["spec"].(map[string]interface{})["destination"].(map[string]interface{})["namespace"].(string)
	objs, err := g.getAllObjects()
	if err != nil {
		return n, err
	}

	// Maps to track direct and indirect relationships
	resourceMap := make(map[string]*unstructured.Unstructured)
	directChildren := make(map[string]*unstructured.Unstructured)
	childMap := make(map[string][]*unstructured.Unstructured)

	for _, obj := range objs {
		// Process AppProject relationships
		if obj.GetKind() == "AppProject" && obj.GetAPIVersion() == "argoproj.io/v1alpha1" {
			if obj.GetName() == projName {
				g.graph.Relationship(n, obj.GetKind(), g.graph.Node(obj.GroupVersionKind(), obj))
			}
		}

		// Populate childMap for indirect relationships
		uid := string(obj.GetUID())
		resourceMap[uid] = obj
		if g.isDirectChild(app, obj) {
			directChildren[uid] = obj
		}
		for _, ownerRef := range obj.GetOwnerReferences() {
			parentUID := string(ownerRef.UID)
			childMap[parentUID] = append(childMap[parentUID], obj)
		}
	}

	// Build graph relationships for direct children
	for _, child := range directChildren {
		childNode := g.graph.Node(child.GroupVersionKind(), child)
		g.graph.Relationship(n, child.GetKind(), childNode)
		// Recursively process indirect children
		g.buildIndirectGraph(childNode, child, childMap)
	}
	return n, nil
}

func (g *ArgoCDGraph) buildIndirectGraph(node *Node, obj *unstructured.Unstructured, childMap map[string][]*unstructured.Unstructured) {
	uid := string(obj.GetUID())
	// Process children associated with this resource
	for _, child := range childMap[uid] {
		childNode := g.graph.Node(child.GroupVersionKind(), child)
		g.graph.Relationship(node, child.GetKind(), childNode)
		// Recursively process the child's descendants
		g.buildIndirectGraph(childNode, child, childMap)
	}
}

// ApplicationSet adds a v1alpha1.ApplicationSet resource to the Graph.
func (g *ArgoCDGraph) ApplicationSet(appset *unstructured.Unstructured) (*Node, error) {
	// Create a node for the ApplicationSet
	n := g.graph.Node(appset.GroupVersionKind(), appset)

	// Fetch all applications across all namespaces
	allApplications, err := g.getAllApplications()
	if err != nil {
		return nil, err
	}

	// Filter applications that are owned by the given ApplicationSet
	appsetUID := string(appset.GetUID())
	ownedApplications := []*unstructured.Unstructured{}

	for _, app := range allApplications {
		for _, ownerRef := range app.GetOwnerReferences() {
			if ownerRef.UID == types.UID(appsetUID) && ownerRef.Kind == "ApplicationSet" {
				ownedApplications = append(ownedApplications, app)
				break
			}
		}
	}

	// Process each owned application
	for _, app := range ownedApplications {
		// Use the existing Application function to process each application and its children
		appNode, err := g.Application(app)
		if err != nil {
			return nil, err
		}

		// Build the relationship between the ApplicationSet and the owned Application
		g.graph.Relationship(n, app.GetKind(), appNode)
	}

	return n, nil
}

// AppProject adds a v1alpha1.AppProject resource to the Graph.
func (g *ArgoCDGraph) AppProject(obj *unstructured.Unstructured) (*Node, error) {
	// Create a node for the AppProject
	n := g.graph.Node(obj.GroupVersionKind(), obj)

	// Fetch all applications across all namespaces
	allApplications, err := g.getAllApplications()
	if err != nil {
		return nil, err
	}

	// Get the name of the AppProject
	appProjectName := obj.GetName()

	// Filter applications that belong to this AppProject
	for _, app := range allApplications {
		spec, ok := app.Object["spec"].(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("invalid spec in application: %v", app.GetName())
		}

		projectName, ok := spec["project"].(string)
		if !ok {
			return nil, fmt.Errorf("project field not found or invalid in application: %v", app.GetName())
		}

		// Check if the application belongs to the current AppProject
		if projectName == appProjectName {
			// Use the existing Application function to process the application and its child resources
			appNode, err := g.Application(app)
			if err != nil {
				return nil, err
			}

			// Build the relationship between the AppProject and the associated Application
			g.graph.Relationship(n, app.GetKind(), appNode)
		}
	}

	return n, nil
}

func (g *ArgoCDGraph) getAllObjects() ([]*unstructured.Unstructured, error) {
	apiResources, err := g.graph.clientset.Discovery().ServerPreferredResources()
	if err != nil {
		return nil, err
	}
	objs := make([]*unstructured.Unstructured, 0, len(apiResources))
	var wg sync.WaitGroup
	for _, apiResource := range apiResources {
		results := make(map[string][]*unstructured.Unstructured, len(apiResource.APIResources))
		lock := &sync.Mutex{}
		for _, api := range apiResource.APIResources {
			wg.Add(1)
			gvk := schema.FromAPIVersionAndKind(apiResource.GroupVersion, apiResource.Kind)
			gv := gvk.GroupVersion()
			gvr := gv.WithResource(api.Name)
			go g.fetchObjectsForResource(gvr, results, &wg, lock)
		}
		wg.Wait()
		for _, resourceObjs := range results {
			objs = append(objs, resourceObjs...)
		}
	}

	return objs, nil
}

func (g *ArgoCDGraph) fetchObjectsForResource(gvr schema.GroupVersionResource, results map[string][]*unstructured.Unstructured, wg *sync.WaitGroup, lock *sync.Mutex) {
	defer wg.Done()
	defer lock.Unlock()
	objList, err := dynamic.New(g.graph.clientset.RESTClient()).Resource(gvr).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		//fmt.Printf("ignoring error : could not find resources for gvr %v\n", gvr)
		lock.Lock()
		results[gvr.String()] = make([]*unstructured.Unstructured, 0)
		return
	}
	result := make([]*unstructured.Unstructured, 0, len(objList.Items))
	for _, obj := range objList.Items {
		result = append(result, &obj)
	}
	lock.Lock()
	results[gvr.String()] = result
}

// Helper to check if an object is a direct child of an application
func (g *ArgoCDGraph) isDirectChild(app *unstructured.Unstructured, obj *unstructured.Unstructured) bool {
	annotations := obj.GetAnnotations()
	labels := obj.GetLabels()

	trackingID, idExists := annotations["argocd.argoproj.io/tracking-id"]
	trackingLabel, labelExists := labels["app.kubernetes.io/instance"]

	return (idExists && strings.HasPrefix(trackingID, fmt.Sprintf("%s:", app.GetName()))) ||
		(labelExists && trackingLabel == app.GetName())
}

// Helper function to fetch all applications across all namespaces
func (g *ArgoCDGraph) getAllApplications() ([]*unstructured.Unstructured, error) {
	gvr := schema.GroupVersionResource{
		Group:    "argoproj.io",
		Version:  "v1alpha1",
		Resource: "applications",
	}

	// Fetch all applications (cluster-wide)
	appList, err := dynamic.New(g.graph.clientset.RESTClient()).
		Resource(gvr).
		Namespace(metav1.NamespaceAll). // NamespaceAll fetches resources from all namespaces
		List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	apps := make([]*unstructured.Unstructured, len(appList.Items))
	for i, app := range appList.Items {
		apps[i] = app.DeepCopy()
	}

	return apps, nil
}
