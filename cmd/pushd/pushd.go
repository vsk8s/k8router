package main

import (
    "fmt"
    "time"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/client-go/kubernetes/typed/extensions/v1beta1"
    "k8s.io/client-go/rest"
)

func main() {
    config, err := rest.InClusterConfig()
    if err != nil {
        panic(err.Error())
    }

    client, err := v1beta1.NewForConfig(config)
    if err != nil {
        panic(err.Error())
    }

    for {
        items, err := client.Ingresses("dev").List(metav1.ListOptions{})

        if err != nil {
            fmt.Printf(err.Error())
            fmt.Printf("\n")
        } else {
            fmt.Printf("There are %d ingresses in the cluster\n", len(items.Items))
        }

        time.Sleep(1000 * time.Millisecond)
    }
}
