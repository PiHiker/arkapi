# ArkAPI Wallet Notes Template

These wallets are for Signet or test environments unless you explicitly replace them.
Never commit real seed phrases, mnemonics, or private keys.

## Merchant Wallet
- Container: bark
- Volume: arkapi_bark-data
- Fingerprint: <record_locally>
- Seed phrase:
  - store privately outside git

## Consumer Wallet
- Container: bark-consumer
- Volume: arkapi_bark-consumer-data
- Fingerprint: <record_locally>
- Seed phrase:
  - store privately outside git

## Recovery
Use your private mnemonic from secure storage when restoring a wallet.
