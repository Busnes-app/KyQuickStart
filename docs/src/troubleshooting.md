# Troubleshooting

- **Product absent from the current catalog:** check current release support; a catalog design or roadmap is not an installable implementation.
- **SSH refused:** verify agent keys, target login and the trusted host fingerprint. Investigate a changed host key through a trusted channel.
- **Preflight fails:** fix the prerequisite and rerun before applying.
- **Resume cannot find the plan:** use the same state directory rather than creating a second installation record.
- **SSO works but ownership is missing:** verify application administrator grants and directory lifecycle delivery in each product.
- **Edge checks fail:** inspect HTTPS hostnames, proxy routing and trusted forwarded headers using the edge guide.

When reporting a problem, include the product version, platform, failed task and a redacted error. Exclude pairing links, device secrets, recovery codes, custodian shares and private content.
