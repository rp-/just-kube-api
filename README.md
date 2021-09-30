# just-kube-api

Start a local kubernetes control plain. This can be used to test the Kubernetes API without spinning up a full cluster

## Usage

```
$ go run gitlab.at.linbit.com/mwanzenboeck/just-kube-api/cmd/just-kube-api
API ready! KubeConfig written to 'kubeconfig'
```

In a different shell, you can now

```
$ export KUBECONFIG=kubeconfig
$ kubectl get namespaces
NAME              STATUS   AGE
default           Active   52s
kube-node-lease   Active   53s
kube-public       Active   53s
kube-system       Active   53s
```

To stop the control plane again, just interrupt the `go run command`
