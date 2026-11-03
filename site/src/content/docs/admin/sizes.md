---
title: Sizes
description: The CPU, memory and GPU sizes people pick for apps and jobs, who may use which, and custom amounts.
sidebar:
  order: 4
---

People pick a **size** for each app and job. The app's container gets the size's CPU and memory as both its request and its limit, replacing the template's own.

```yaml
sizes:
  - {name: small, displayName: Small, cpu: "1", memory: 2Gi}
  - {name: medium, displayName: Medium, cpu: "4", memory: 8Gi}
  - {name: large, displayName: Large, cpu: "8", memory: 32Gi}
defaultSize: small
```

- `name` is stored on each app; keep it once apps use it. `displayName` is what people see.
- `defaultSize` is what new apps get when nobody picks one.
- An empty list offers no choice, and apps keep their template's resources.
- `allowedGroups: [group, ...]` limits a size to those groups, and `allowedProviderRoles: [role, ...]` to holders of those identity provider roles (see [Limit templates and sizes by provider role](/admin/sign-in/#limit-templates-and-sizes-by-provider-role)); either one is enough. Platform admins may always use it; others do not see it.

## Sizes that do not fit

A size bigger than every node's free CPU or memory is shown disabled and refused, and an app already at it fails as **Size too big for any machine**. Set `allowUnschedulable: true` on a size that a cluster autoscaler can add a node for: it is offered and waits for that node.

## GPU sizes

`gpu: N` gives the app N GPUs (`nvidia.com/gpu` by default; set `gpuResource`, for example `amd.com/gpu`, for others). `nodeSelector` and `tolerations` place the pod on GPU nodes that are tainted to keep other pods off; both also work on sizes without GPUs.

```yaml
sizes:
  - {name: small, displayName: Small, cpu: "1", memory: 2Gi}
  - name: gpu
    displayName: GPU
    cpu: "8"
    memory: 32Gi
    gpu: 1
    nodeSelector: {nvidia.com/gpu.present: "true"}
    tolerations: [{key: nvidia.com/gpu, operator: Exists, effect: NoSchedule}]
    allowedGroups: [ml]
```

A GPU size fits only when one node has the CPU, memory, GPUs and labels together. A default size that does not fit never falls back to a GPU size.

## Custom CPU and memory

`customSize` lets some people type their own amounts instead of a size, up to a limit. A rule matches holders of any of its roles (`platform-admin`, `template-admin`, `member`), members of any of its groups, or holders of any of its `providerRoles` (identity provider roles); a person matching several rules gets the largest of each amount. An empty maximum means no limit but the largest machine.

```yaml
customSize:
  enabled: true
  rules:
    - roles: [platform-admin]
    - {groups: [data-science], maxCPU: "4", maxMemory: 16Gi}
```
