# Changelog

The changes of each release are in its notes on GitHub Releases:
https://github.com/giantswarm/giantswarm-platform-manager/releases

## [Unreleased]

### Added

- agent-platform: configure agent-manager credential operations with a client-owned HTTPS URL and a dedicated RFC 8693 broker client restricted to its GitHub App user grant. Reconciliation preserves both choices and generates the broker credential through the existing encrypted secret machinery.
