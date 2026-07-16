FROM node:22-bookworm-slim

ARG CODEX_PACKAGE=@openai/codex

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl git ripgrep bash \
    && npm install --global "${CODEX_PACKAGE}" \
    && rm -rf /var/lib/apt/lists/* /root/.npm

RUN useradd --create-home --uid 1000 --shell /bin/bash nox \
    && mkdir -p /workspace /home/nox/.codex \
    && chown -R nox:nox /workspace /home/nox

USER nox
WORKDIR /workspace
ENV HOME=/home/nox
ENV CODEX_HOME=/home/nox/.codex

CMD ["sleep", "infinity"]
