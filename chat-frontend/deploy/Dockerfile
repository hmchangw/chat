FROM node:22-alpine AS builder
WORKDIR /app
COPY chat-frontend/package.json chat-frontend/package-lock.json ./
RUN npm ci
COPY chat-frontend/ .
RUN npm run build

FROM nginx:alpine
COPY --from=builder /app/dist /usr/share/nginx/html
EXPOSE 80
CMD ["nginx", "-g", "daemon off;"]
