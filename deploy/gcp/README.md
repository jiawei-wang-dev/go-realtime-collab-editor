# Google Cloud Terraform

This directory prepares the Phase 9.0 Compute Engine deployment. It does not run `terraform apply`.

## Prerequisites

1. Create a Google Cloud project and a versioned, locked GCS state bucket.
2. Push the server image to Artifact Registry and pin `app_image` by digest.
3. Create the runtime environment secret and per-node TLS bundle secrets. Each node certificate must contain the configured node ID and incarnation in its SPIFFE URI SAN.
4. Create an etcd TLS bundle whose server certificate covers all three reserved private addresses.
5. Copy `terraform.tfvars.example` to a file outside source control and replace every placeholder.

## Validation

```bash
terraform fmt -check -recursive
terraform init -backend=false
terraform validate
```

For a reviewed plan, initialize the remote backend and use a saved plan. Never put secrets on the command line:

```bash
terraform init -backend-config="bucket=YOUR_LOCKED_STATE_BUCKET" -backend-config="prefix=collab/prod"
terraform plan -out=collab-prod.tfplan
terraform show collab-prod.tfplan
```

The generated MySQL password and Redis authentication string are stored in Terraform state and Secret Manager. Application startup metadata contains only Secret Manager IDs; each VM fetches the values at boot. Secret access is granted per secret rather than at project scope. The state bucket therefore requires encryption, versioning, retention, uniform access and a narrowly scoped IAM policy.

The configuration deliberately creates three stable app VMs and three etcd VMs. QUIC ownership redirects require a stable, directly routable UDP endpoint for each app node; a generic load balancer address is not a node identity.
