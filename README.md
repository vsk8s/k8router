# k8router
[![Build Status](https://img.shields.io/circleci/build/github/vsk8s/k8router?style=flat)](https://circleci.com/gh/vsk8s/k8router)
[![Go Report Card](https://goreportcard.com/badge/github.com/vsk8s/k8router?style=flat)](https://goreportcard.com/report/github.com/vsk8s/k8router)
[![codecov](https://codecov.io/gh/vsk8s/k8router/branch/master/graph/badge.svg)](https://codecov.io/gh/vsk8s/k8router)
[![Go Doc](https://img.shields.io/badge/godoc-reference-blue.svg?style=flat)](http://godoc.org/github.com/vsk8s/k8router)
[![Release](https://img.shields.io/github/tag/vsk8s/k8router.svg?style=flat)](https://github.com/vsk8s/k8router/releases/latest)
[![FOSSA Status](https://app.fossa.io/api/projects/git%2Bgithub.com%2Fvsk8s%2Fk8router.svg?type=shield)](https://app.fossa.io/projects/git%2Bgithub.com%2Fvsk8s%2Fk8router?ref=badge_shield)

This is a software project by SOSETH and VIS to manage several external
load-balancers which forward traffic to several Kubernetes clusters.

Externally expose kubernetes ingresses using HAProxy and terminate TLS for them.

## Problem Description

Given a set of n Kubernetes clusters and m pairs of load-balancers to each of
them, the goal of the project is to transport information about available
domains in each cluster to all load-balancers such that any router is able to
forward traffic any cluster.

### Open Tasks
 * Figure out why update/patch on the fake client set doesn't get propagated.
   This prevents us from writing more interesting unit tests...
 * systemd integration (instead of doing `sudo systemctl ...`)

### Prerequisites

* HAProxy, installed and configured to use a conf.d-style configuration format.
  Have a look [here](https://github.com/SOSETH/haproxy) for more information.
* Kubernetes clusters and client configurations for a `k8router` user.
    * The user must be able to watch Ingresses in all namespaces in all clusters.
      See `k8s-rbac.yml` for more information.
* Certificates for all your domains.

Each Kubernetes cluster has to expose its API to all the routers. Every kubelet
node has to be accessible by all routers.

### Configuration

An example configuration might look like this:

```
haproxyDropinPath: /etc/haproxy/conf.d/90-k8router.conf
haproxyTemplatePath: /root/template
clusters:
  - name: local
    kubeconfig: /etc/k8router/k8s/kubeconfig.yml
certificates:
  - cert: /foo
    name: realcert
    domains:
      - example.org
  - cert: /bar
    name: dummycert
    domains:
      - '*.org'
      - '*.com'
ips:
  - 1.2.3.4
```

This will generate a configuration at `/etc/haproxy/conf.d/90-k8router.conf`
from `/root/template` for one cluster (`/etc/k8router/k8s/kubeconfig.yml`),
using two certificates and one external IP. An example template file is included
[here](template), note that the certificates are specified as directories (see
the [HAProxy
docs](https://cbonte.github.io/haproxy-dconv/1.9/configuration.html#5.1-crt) on
this one).

### Running

Execute `./k8router -verbose -config <path/to/config>` in a terminal, the log
output should tell you if something goes wrong. Due to missing systemd
integration we still require passwordless sudo for the service user.


## License
[![FOSSA Status](https://app.fossa.io/api/projects/git%2Bgithub.com%2Fvsk8s%2Fk8router.svg?type=large)](https://app.fossa.io/projects/git%2Bgithub.com%2Fvsk8s%2Fk8router?ref=badge_large)