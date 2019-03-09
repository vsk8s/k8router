package main

import (
    "fmt"
    "time"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/client-go/kubernetes/typed/extensions/v1beta1"
    "k8s.io/client-go/kubernetes"
    "k8s.io/client-go/rest"
)

func main() {
    config, err := rest.InClusterConfig()
    if err != nil {
        panic(err.Error())
    }

    coreclient, err := kubernetes.NewForConfig(config)
    if err != nil {
        panic(err.Error())
    }

    client, err := v1beta1.NewForConfig(config)
    if err != nil {
        panic(err.Error())
    }

    for {

        namespaces, err := coreclient.CoreV1().Namespaces().List(metav1.ListOptions{})
        if err != nil {
            fmt.Printf("%s\n", err.Error())
        }

        for i := 0; i < len(namespaces.Items); i++ {

            nsName := namespaces.Items[i].ObjectMeta.Name

            items, err := client.Ingresses(nsName).List(metav1.ListOptions{})

            if err != nil {
                fmt.Printf("%s\n", err.Error())
            } else {
                fmt.Printf("There are %d ingresses in namespace '%s'\n",
                    len(items.Items),
                    nsName)
            }
        }

        time.Sleep(1000 * time.Millisecond)
    }
}
