package clusters

import (
	"context"
	"fmt"
	reapergo "github.com/jsanda/reaper-client-go/reaper"
	"github.com/thelastpickle/reaper-operator/pkg/apis/reaper/v1alpha1"
	"math"
	"runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"time"
)

const (
	syncClusters = "cassandra-reaper.io/cassandra-reaper.io/syncClusters"
)

var log = logf.Log.WithName("clusters-monitor")

type ClusterFilter func (cluster v1alpha1.CassandraCluster) bool

func ClusterNameFilter(name string) ClusterFilter {
	return func(cluster v1alpha1.CassandraCluster) bool {
		return cluster.Name == name
	}
}

func StartMonitor(namespace string, c client.Client, stopCh <-chan struct{}) {
	log.Info("starting")

	numWorkers := int(math.Min(5, float64(runtime.NumCPU())))
	reaperCh := make(chan v1alpha1.Reaper)
	ctx := context.Background()

	startWorkers(ctx, c, numWorkers, reaperCh)

	tick := time.Tick(10 * time.Second)

	Loop:
		for {
			select {
			case <-tick:
				scheduleChecks(ctx, c, namespace, reaperCh)
			case <-stopCh:
				break Loop
			}
		}

	log.Info("stopping")
}

func startWorkers(ctx context.Context, c client.Client, numWorkers int, reaperCh <-chan v1alpha1.Reaper) {
	for i := 0; i < numWorkers; i++ {
		go checkReapers(ctx, c, reaperCh)
	}
}

// This function first queries the API server for a ReaperList. It then iterates over the
// list and adds each Reaper to the Reaper channel for processing by the worker goroutines.
func scheduleChecks(ctx context.Context, c client.Client, namespace string, reaperCh chan<- v1alpha1.Reaper) {
	getListCtx, cancel := context.WithTimeout(ctx, 10 * time.Second)
	defer cancel()

	if reaperList, err := getReaperList(getListCtx, namespace, c); err == nil {
		for _, reaper := range reaperList.Items {
			select {
			case reaperCh <- reaper:
			case <-ctx.Done():
				log.Error(ctx.Err(), "did not finish scheduling next round of checks")
			}
		}
	} else {
		log.Error(err, "failed to get ReaperList")
	}
}

func getReaperList(ctx context.Context, namespace string, c client.Client) (*v1alpha1.ReaperList, error) {
	list := &v1alpha1.ReaperList{}
	opts := []client.ListOption{
		client.InNamespace(namespace),
	}
	err := c.List(ctx, list, opts...)
	return list, err
}

func checkReapers(ctx context.Context, c client.Client, reaperCh <-chan v1alpha1.Reaper) {
	for reaper := range reaperCh {
		// TODO check that .Spec.Clusters matches .Status.Clusters.
		//      If the desired state does not match actual state with respect to managed
		//      cluster, then ignore this Reaper object until state converges.

		// Fetch the registered clusters and compare against what is in the spec. If they
		// differ set an annotation on the Reaper. Then save the changes to the Reaper get
		// queued for reconciliation

		if restClient, err := createRESTClient(&reaper); err == nil {
			actualClusters := make([]*reapergo.Cluster, 0)
			for result := range restClient.GetClusters(ctx) {
				if result.Error == nil {
					actualClusters = append(actualClusters, result.Cluster)
				} else {
					// TODO error on safe side and queue for reconciliation
					log.Info("failed to get a cluster", "Reaper.Namespace", reaper.Namespace,
						"Reaper.Name", reaper.Name)
				}

				// We are not concerned with all clusters, only managed clusters, i.e.,
				// those listed in the spec.
				if !clustersMatch(&reaper, actualClusters) {
					// A managed cluster has been removed out of band, either through the Reaper UI
					// or REST API. We add an annotation so that the Reaper object gets queued for
					// reconciliation.
					if reaper.Annotations == nil {
						reaper.Annotations = make(map[string]string)
					}
					reaper.Annotations[syncClusters] = "true"
				}
				if err := c.Update(ctx, &reaper); err != nil {
					log.Error(err, "failed to update Reaper with annotation", "Reaper.Namespace",
						reaper.Namespace, "Reaper.Name", reaper.Name, "Annotation", syncClusters)
				}
			}
		} else {
			log.Error(err, "failed to create REST client", "Reaper.Namespace", reaper.Namespace,
				"Reaper.Name", reaper.Name)
		}
	}
}

func createRESTClient(reaper *v1alpha1.Reaper) (reapergo.ReaperClient, error) {
	if restClient, err := reapergo.NewReaperClient(fmt.Sprintf("http://%s.%s:8080", reaper.Name, reaper.Namespace)); err == nil {
		return restClient, nil
	} else {
		return nil, fmt.Errorf("failed to create REST client: %w", err)
	}
}

// Returns true if each of reaper.Spec.Clusters is found in actualClusters
func clustersMatch(reaper *v1alpha1.Reaper, actualClusters []*reapergo.Cluster) bool {
	for i := range reaper.Spec.Clusters {
		if findCluster(reaper.Spec.Clusters, ClusterNameFilter(actualClusters[i].Name)) == nil {
			return false
		}
	}
	return true
}

func findCluster(clusters []v1alpha1.CassandraCluster, filter ClusterFilter) *v1alpha1.CassandraCluster {
	for _, cluster := range clusters {
		if filter(cluster) {
			return &cluster
		}
	}
	return nil
}
