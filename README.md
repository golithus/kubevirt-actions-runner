# Kubevirt Actions Runner

<!-- markdown-link-check-disable-next-line -->

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
[![GitHub Super-Linter](https://github.com/electrocucaracha/kubevirt-actions-runner/workflows/Lint%20Code%20Base/badge.svg)](https://github.com/marketplace/actions/super-linter)

<!-- markdown-link-check-disable-next-line -->

![visitors](https://visitor-badge.laobi.icu/badge?page_id=electrocucaracha.kubevirt-actions-runner)
[![Scc Code Badge](https://sloc.xyz/github/electrocucaracha/kubevirt-actions-runner?category=code)](https://github.com/boyter/scc/)
[![Scc COCOMO Badge](https://sloc.xyz/github/electrocucaracha/kubevirt-actions-runner?category=cocomo)](https://github.com/boyter/scc/)

## Summary

`kubevirt-actions-runner` is a runner image for [Actions Runner Controller (ARC)](https://github.com/actions/actions-runner-controller) that spawns ephemeral virtual machines for jobs using [KubeVirt](https://kubevirt.io).

## Use cases

- Windows and macOS jobs
- Jobs that require configuring system services
- Jobs that require stronger isolation

## Usage

You need a Kubernetes cluster with [Actions Runner Controller](https://github.com/actions/actions-runner-controller/blob/master/docs/quickstart.md) and [KubeVirt](https://kubevirt.io/quickstart_cloud) installed.

### 1. Create VirtualMachine template

First, we need to create a VirtualMachine to act as a template for the runner VMs.
`kubevirt-actions-runner` will create VirtualMachineInstances from it, and the VirtualMachine itself will never be started.

Create a namespace and apply the sample template:

```bash
! kubectl get namespaces "${namespace}" && kubectl create namespace "${namespace}"
kubectl apply -f scripts/vm_template.yml -n "${namespace}"
```

Let's take a deeper look at this sample VirtualMachine.
Inside we mount the `runner-info` volume:

```yaml
apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: ubuntu-jammy-vm
spec:
  runStrategy: Manual
  template:
    spec:
      domain:
        devices:
          filesystems:
            - name: runner-info
              virtiofs: {}
```

This `runner-info` volume will be injected by `kubevirt-actions-runner`, containing `runner-info.json` that looks like the following:

```json
{
  "name": "runner-abcde-abcde",
  "token": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
  "url": "https://github.com/org/repo",
  "ephemeral": true,
  "groups": "",
  "labels": ""
}
```

### 2. Set up RBAC

The service account of the runner pod needs to be able to create `VirtualMachineInstance`s.
An example is as follows:

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: kubevirt-actions-runner
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: kubevirt-actions-runner
rules:
  - apiGroups: ["kubevirt.io"]
    resources: ["virtualmachines"]
    verbs: ["get", "watch", "list"]
  - apiGroups: ["kubevirt.io"]
    resources: ["virtualmachineinstances"]
    verbs: ["get", "watch", "list", "create", "delete"]
  - apiGroups: ["cdi.kubevirt.io"]
    resources: ["datavolumes"]
    verbs: ["get", "watch", "list", "create", "delete"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: cdi-cloner
rules:
  - apiGroups: ["cdi.kubevirt.io"]
    resources: ["datavolumes/source"]
    verbs: ["create"]
```

### 3. Create runner scale set

You can configure the runner scale set using Helm.
Use the following `values.yaml`:

```yaml
githubConfigUrl: https://github.com/<your_enterprise/org/repo>
githubConfigSecret: ...
template:
  spec:
    serviceAccountName: kubevirt-actions-runner
    containers:
      - name: runner
        image: electrocucaracha/kubevirt-actions-runner:latest
        command: []
        env:
          - name: KUBEVIRT_VM_TEMPLATE
            value: ubuntu-jammy-vm
          - name: RUNNER_NAME
            valueFrom:
              fieldRef:
                fieldPath: metadata.name
```

```bash
helm upgrade --create-namespace --namespace "${namespace}" \
    --wait --install --values values.yml vm-self-hosted \
    oci://ghcr.io/actions/actions-runner-controller-charts/gha-runner-scale-set
```

The lifecycle of the spawned VMI is bound to the runner pod.
If one of them exits, the other will be terminated as well.
