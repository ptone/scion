# The hub_sa_minting opt-in: off by default (no new IAM, so existing hubs
# plan unchanged), and when enabled, exactly one unconditioned project-level
# roles/iam.serviceAccountAdmin grant for the hub SA.
mock_provider "google" {}

variables {
  project_id     = "tfha-test-project"
  project_number = "123456789012"
  hub_name       = "tfha-h3"
}

# The opt-in run applies against the mock so the hub SA's email is known
# when asserting the grant's member. The provider validates
# service_account_id's format even against the mock, so name/email are
# pinned for every SA another resource references.
override_resource {
  target = google_service_account.hub
  values = {
    name  = "projects/tfha-test-project/serviceAccounts/tfha-h3-hub@tfha-test-project.iam.gserviceaccount.com"
    email = "tfha-h3-hub@tfha-test-project.iam.gserviceaccount.com"
  }
}

override_resource {
  target = google_service_account.transport
  values = {
    name  = "projects/tfha-test-project/serviceAccounts/tfha-h3-transport@tfha-test-project.iam.gserviceaccount.com"
    email = "tfha-h3-transport@tfha-test-project.iam.gserviceaccount.com"
  }
}

run "default_creates_no_minting_grant" {
  command = plan

  assert {
    condition     = length(google_project_iam_member.hub_sa_minting) == 0
    error_message = "hub_sa_minting defaults to false and must then create no serviceAccountAdmin grant."
  }

  assert {
    condition     = output.hub_sa_minting_enabled == false
    error_message = "hub_sa_minting_enabled must be false when the opt-in is unset."
  }

  assert {
    condition     = length(output.hub_iam_grants) == 7
    error_message = "the default must leave the boot-gating hub_iam_grants list unchanged."
  }
}

run "opt_in_grants_exactly_one_unconditioned_service_account_admin" {
  command = apply

  variables {
    hub_sa_minting = true
  }

  assert {
    condition     = length(google_project_iam_member.hub_sa_minting) == 1
    error_message = "the opt-in must create exactly one minting grant."
  }

  assert {
    condition     = google_project_iam_member.hub_sa_minting[0].role == "roles/iam.serviceAccountAdmin"
    error_message = "the minting grant must be roles/iam.serviceAccountAdmin (serviceAccountCreator lacks setIamPolicy)."
  }

  assert {
    condition     = google_project_iam_member.hub_sa_minting[0].project == "tfha-test-project"
    error_message = "the minting grant must be on var.project_id."
  }

  assert {
    condition     = google_project_iam_member.hub_sa_minting[0].member == "serviceAccount:tfha-h3-hub@tfha-test-project.iam.gserviceaccount.com"
    error_message = "the minting grant's member must be the hub SA and nothing else."
  }

  assert {
    condition     = length(google_project_iam_member.hub_sa_minting[0].condition) == 0
    error_message = "the minting grant must carry no condition (GCP cannot name-scope it, and condition fields are ForceNew)."
  }

  assert {
    condition     = output.hub_sa_minting_enabled == true
    error_message = "hub_sa_minting_enabled must be true when the opt-in is set."
  }
}
