FROM node:22-bookworm-slim AS build
WORKDIR /web
COPY package.json package-lock.json .npmrc ./
COPY scripts/check-node.cjs ./scripts/check-node.cjs
RUN npm ci
COPY . .
RUN npm run build

# Export with: docker build --output type=local,dest=web/dist web
FROM scratch AS artifact
COPY --from=build /web/dist/ /
