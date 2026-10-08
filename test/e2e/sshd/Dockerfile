FROM docker:29.4-cli@sha256:51e23845f5caff1e688a2fae003b0c69d635c9200ad544731db1593731df1d3a
RUN apk add --no-cache openssh-server
ENTRYPOINT ["/usr/sbin/sshd", "-D", "-e", "-h", "/keys/host_ed25519", "-o", "AuthorizedKeysFile=/keys/authorized_keys", "-o", "StrictModes=no", "-o", "PasswordAuthentication=no", "-o", "PermitRootLogin=prohibit-password"]
