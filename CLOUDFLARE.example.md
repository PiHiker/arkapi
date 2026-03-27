Cloudflare Workers AI setup for ArkAPI

Keep the real account ID and token in your private .env file or another secret store.
Do not commit live bearer tokens to git.

Suggested env vars:
- ARKAPI_CLOUDFLARE_AI_ACCOUNT_ID=<your_account_id>
- ARKAPI_CLOUDFLARE_AI_TOKEN=<your_api_token>
- ARKAPI_CLOUDFLARE_AI_MODEL=@cf/meta/llama-3-8b-instruct

Verify token:

curl "https://api.cloudflare.com/client/v4/user/tokens/verify" \
  -H "Authorization: Bearer <your_cloudflare_token>"

Run a sample Workers AI request:

curl \
  "https://api.cloudflare.com/client/v4/accounts/<your_account_id>/ai/run/@cf/meta/llama-3-8b-instruct" \
  -H "Authorization: Bearer <your_cloudflare_token>" \
  -H "Content-Type: application/json" \
  -d {messages:[role:user]}
