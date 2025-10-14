# Changelog

> [!WARNING]
> This changelog is an archive. Future changes are documented in the projects [GitHub releases](https://github.com/telekom/gateway-rotator/releases).

# 1.0.0 (2025-05-28)

### Bug Fixes

* add check for tls fields in source secret ([f6d2c91](https://github.com/telekom/gateway-rotator/commit/f6d2c91d6fb6d0620ef5f3c6780bdcd5bd74cdaa))
* add predicate for target name annotation ([833f52b](https://github.com/telekom/gateway-rotator/commit/833f52b51b155699048ce6ffe75de9d9f816da56))
* clusterwide overlay deploys namespace resource ([c00945d](https://github.com/telekom/gateway-rotator/commit/c00945d64e1475fefb1914d216d4c259e9779618))

### Features

* add ability to run namespaced ([24bcc7b](https://github.com/telekom/gateway-rotator/commit/24bcc7bd1cfaad5b52d228c535fc24f1308b44de))
* allow generation and rotation of kids based on certificates ([b96ee43](https://github.com/telekom/gateway-rotator/commit/b96ee430ac710ab55eb8e35d5aae43e39095c420))
* do not delete target secret if source is deleted ([96dbc0b](https://github.com/telekom/gateway-rotator/commit/96dbc0beffbcebae213eb4b034e61776ac2ceb1f))
* hardened securityContexts for pod and container ([b274199](https://github.com/telekom/gateway-rotator/commit/b2741997d42890df7e7eefd693d25f2bd2f1a648))
* introduce semantic releasing ([5e251d2](https://github.com/telekom/gateway-rotator/commit/5e251d26136fe1abb5f85bbdec9d4361902a8649))
* namespace list configuration only for scopes ([d622cf1](https://github.com/telekom/gateway-rotator/commit/d622cf120dc2d688f627921f305efcf25d33a823))
