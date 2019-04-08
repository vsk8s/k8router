package router

import (
	"github.com/onsi/gomega"
	"github.com/soseth/k8router/pkg/config"
	v1coreapi "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"testing"
)

// Test basic event handling by pointing the cluster handler to an empty mock fake client, producing some events and
// then comparing state
func TestClusterBasicEventHandling(t *testing.T) {
	g := gomega.NewGomegaWithT(t)
	client := fake.NewSimpleClientset(&v1coreapi.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "ingress-nginx",
		},
	})
	uut := ClusterFromConfig(config.Cluster{
		Name: "fake",
	})
	uut.extensionClient = client.ExtensionsV1beta1()
	uut.coreClient = client.CoreV1()
	go func () {
		err := uut.watch()
		if err != nil {
			t.Fatal(err)
		}
	}()
	// Wait until UUT signals readiness
	_ = <- uut.readinessChannel
	// Create pod
	_, err := client.CoreV1().Pods("ingress-nginx").Create(&v1coreapi.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "ingress-nginx",
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
	clusterState := <- uut.clusterStateChannel
	g.Expect(len(clusterState.Ingresses)).To(gomega.BeIdenticalTo(0))
	g.Expect(len(clusterState.Backends)).To(gomega.BeIdenticalTo(1))
	uut.Stop()
}
