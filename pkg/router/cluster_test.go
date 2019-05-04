package router

import (
	"github.com/onsi/gomega"
	"github.com/soseth/k8router/pkg/config"
	"github.com/soseth/k8router/pkg/state"
	v1coreapi "k8s.io/api/core/v1"
	v1beta1extensionsapi "k8s.io/api/extensions/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	"strconv"
	"testing"
)

// Get a fake kubernetes client and a cluster handler which are linked to each other
func createFakeClientsetAndUUT(t *testing.T, objects ...runtime.Object) (*fake.Clientset, *Cluster) {
	objects = append(objects, &v1coreapi.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "ingress-nginx",
		},
	})
	client := fake.NewSimpleClientset(objects...)
	clusterStateChannel := make(chan state.ClusterState)
	cfg := config.ClusterInternal{
		Name: "fake",
	}
	uut := ClusterFromConfig(config.Cluster{
		&cfg,
	}, clusterStateChannel)
	uut.extensionClient = client.ExtensionsV1beta1()
	uut.coreClient = client.CoreV1()
	go func() {
		err := uut.watch()
		if err != nil {
			t.Fatal(err)
		}
	}()
	// Wait until UUT signals readiness
	_ = <-uut.readinessChannel
	return client, uut
}

// Test basic event handling by pointing the cluster handler to an empty mock fake client, producing a single pod
// event and checking whether it is received correctly
func TestClusterBasicEventHandling(t *testing.T) {
	g := gomega.NewGomegaWithT(t)
	client, uut := createFakeClientsetAndUUT(t)
	// Create pod
	_, err := client.CoreV1().Pods("ingress-nginx").Create(&v1coreapi.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ingress-nginx",
			Namespace: "ingress-nginx",
			Labels: map[string]string{
				"app.kubernetes.io/name": "ingress-nginx",
			},
		},
		Status: v1coreapi.PodStatus{
			PodIP: "1.2.3.4",
		},
	})
	if err != nil {
		t.Error(err)
		return
	}
	// This should give precisely one event
	clusterState := <-uut.clusterStateChannel
	g.Expect(len(clusterState.Ingresses)).To(gomega.BeIdenticalTo(0))
	g.Expect(len(clusterState.Backends)).To(gomega.BeIdenticalTo(1))
	uut.Stop()
}

// Test event handling by pointing the cluster handler to an empty mock fake client, producing some events and
// then comparing state
func TestClusterEventHandling(t *testing.T) {
	g := gomega.NewGomegaWithT(t)
	client, uut := createFakeClientsetAndUUT(t)
	// Create pods
	for i := 0; i < 3; i++ {
		_, err := client.CoreV1().Pods("ingress-nginx").Create(&v1coreapi.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "ingress-nginx-" + strconv.Itoa(i),
				Namespace: "ingress-nginx",
				Labels: map[string]string{
					"app.kubernetes.io/name": "ingress-nginx",
				},
			},
			Status: v1coreapi.PodStatus{
				PodIP: "1.2.3." + strconv.Itoa(i),
			},
		})
		if err != nil {
			t.Error(err)
			return
		}
	}
	// Create ingress

	_, err := client.ExtensionsV1beta1().Ingresses("ingress-nginx").Create(&v1beta1extensionsapi.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "dummy-ingress",
			Namespace: "ingress-nginx",
		},
		Spec: v1beta1extensionsapi.IngressSpec{
			Rules: []v1beta1extensionsapi.IngressRule{
				{
					Host: "test.example.org",
				},
			},
		},
	})
	if err != nil {
		t.Error(err)
		return
	}
	// This should give precisely four events
	clusterState := <-uut.clusterStateChannel
	for i := 0; i < 3; i++ {
		clusterState = <-uut.clusterStateChannel
	}
	g.Expect(len(clusterState.Ingresses)).To(gomega.BeIdenticalTo(1))
	g.Expect(len(clusterState.Backends)).To(gomega.BeIdenticalTo(3))
	uut.Stop()
}
