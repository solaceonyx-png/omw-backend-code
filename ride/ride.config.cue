// The Verification Flow ID from the Stripe dashboard: Identity > Verification flows.
VerificationFlowID: "vf_1U99PhIy3UujDDDdGc3VG7B4"

// Default for cloud deployments — where Stripe redirects the user back to
// after they finish (or exit) the hosted verification page.
ReturnURL: string | *"https://d3ky60yagjwjd2.cloudfront.net/verify/complete"

// Override for local development
if #Meta.Environment.Cloud == "local" {
    ReturnURL: "http://localhost:4200/verify/complete"
}
