ClientID: "JaSdLYvMfXqzgEdEW8h8p6FyvqjJUvkB"
Domain:   "dev-fv5346g2sjdoddhg.us.auth0.com"

// Default for cloud deployments
CallbackURL: string | *"https://d3ky60yagjwjd2.cloudfront.net/"
LogoutURL:   string | *"https://d3ky60yagjwjd2.cloudfront.net/"

// Override for local development
if #Meta.Environment.Cloud == "local" {
    CallbackURL: "http://localhost:4200/"
    LogoutURL:   "http://localhost:4200/"
}