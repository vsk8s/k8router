# k8router

Externally expose kubernetes ingresses using HAProxy and terminate TLS for them.

### Open Tasks
 * Figure out why update/patch on the fake client set doesn't get propagated. This prevents us
   from writing more interesting unit tests...
 * Debian packaging
 * systemd integration (instead of doing `sudo systemctl ...`)
 * Licensing (Apache or something?)
 * CI/CD, RelEng

### Prerequisites

* HAProxy, installed and configured to use a conf.d-style configuration format.
  Have a look [here](https://github.com/SOSETH/haproxy) for more information.
* Kubernetes clusters and client configurations for a `k8router` user.
    * The user must be able to watch Ingresses in all namespaces in all clusters.
      See `k8s-rbac.yml` for more information.
* Certificates for all your domains.

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
This will generate a configuration at `/etc/haproxy/conf.d/90-k8router.conf` from `/root/template` for
one cluster (`/etc/k8router/k8s/kubeconfig.yml`), using two certificates and one external IP. An example
template file is included [here](template), note that the certificates are specified as directories (see the
[HAProxy docs](https://cbonte.github.io/haproxy-dconv/1.9/configuration.html#5.1-crt) on this one).

### Running
Just execute `./k8router -verbose -config <path/to/config>` in a terminal, the log output should tell you
if something goes wrong. At the moment, (due to systemd integration), we still require passwordless sudo
for the service user.