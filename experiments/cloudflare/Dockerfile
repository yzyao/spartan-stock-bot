FROM node:24-bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /probe
COPY package.json package-lock.json ./
RUN npm ci --omit=dev && npm cache clean --force
COPY lib.mjs probe.mjs ./
USER node
ENTRYPOINT ["node", "probe.mjs"]
