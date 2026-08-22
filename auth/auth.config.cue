ClientID: "JaSdLYvMfXqzgEdEW8h8p6FyvqjJUvkB"
Domain: "dev-fv5346g2sjdoddhg.us.auth0.com"

// Default / Cloud values (CloudFront domain)
CallbackURL: "https://d3ky60yagjwjd2.cloudfront.net/"
LogoutURL: "https://d3ky60yagjwjd2.cloudfront.net/"

// An application running locally
if #Meta.Environment.Type == "development" && #Meta.Environment.Cloud == "local" {
	CallbackURL: "http://localhost:4200/"
	LogoutURL: "http://localhost:4200/"
}