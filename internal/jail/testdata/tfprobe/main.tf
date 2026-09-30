# The live content-executing probe (#154): a plan runs this `external` data
# source's program — agent-written code — which tries to read ~/.ssh and to
# reach an outside host. In the host-side jail both must fail while the plan
# itself runs.
terraform {
  required_providers {
    external = {
      source  = "hashicorp/external"
      version = "~> 2.3"
    }
  }
}

data "external" "probe" {
  program = ["sh", "${path.module}/probe.sh"]
}

output "probe" {
  value = data.external.probe.result
}
