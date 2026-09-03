FROM public.ecr.aws/amazonlinux/amazonlinux@sha256:6d8e068b91f351df5bf6acd4bd261316e42747ad4bae76689ff6f4939e2180a2

RUN dnf install -y \
      aws-nitro-enclaves-cli-1.4.5-0.amzn2023 \
      aws-nitro-enclaves-cli-devel-1.4.5-0.amzn2023 \
    && dnf clean all \
    && rm -rf /var/cache/dnf

ENV NITRO_CLI_BLOBS=/usr/share/nitro_enclaves/blobs

ENTRYPOINT ["nitro-cli"]
