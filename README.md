# k8router

An Ingress watcher and HAProxy templater for transparent multi-cluster routing.

### Prerequisites

* HAProxy, installed and configured to use a conf.d-style configuration format.
  Have a look [here](https://github.com/SOSETH/haproxy) for more information.
* Kubernetes clusters and client configurations for a `k8router` user (important: NOT a service account!).
    * `k8router` must be able to watch Ingresses in all namespaces in all clusters.
      See `k8s-rbac.yml` for more information.

### Usage

To be written after configuration is finalized.
