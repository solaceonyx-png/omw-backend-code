ClientID: "JaSdLYvMfXqzgEdEW8h8p6FyvqjJUvkB"
Domain:   "dev-fv5346g2sjdoddhg.us.auth0.com"

// Default for cloud deployments
CallbackURL: *"https://d3ky60yagjwjd2.cloudfront.net/" | string
LogoutURL:   *"https://d3ky60yagjwjd2.cloudfront.net/" | string

Environments: {
    "local": {
        CallbackURL: "http://localhost:4200/"
        LogoutURL:   "http://localhost:4200/"
    }
}