FROM node:16-alpine AS build
WORKDIR /src
COPY server.js .
RUN node --check server.js
ENV NODE_ENV=production

FROM node:16-alpine
WORKDIR /app
COPY --from=build /src/server.js .
USER node
CMD ["node", "server.js"]
