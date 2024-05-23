FROM alpine:3.20
COPY bitswan-gitops /usr/bin/bitswan-gitops
ENTRYPOINT ["/usr/bin/bitswan-gitops"]