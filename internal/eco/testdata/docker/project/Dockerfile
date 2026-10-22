# syntax=docker/dockerfile:1
ARG NODE_VERSION=20.11-alpine
ARG GO_VERSION
ARG REGISTRY=ghcr.io

FROM --platform=$BUILDPLATFORM node:${NODE_VERSION} AS web
RUN npm ci

FROM node:${NODE_VERSION} AS web2

FROM golang:1.22.0-bookworm@sha256:a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90 AS build
COPY --from=web /app /app

FROM golang:${GO_VERSION} AS unresolved-tag
FROM ${REGISTRY}/acme/base:3.2 AS base
FROM build AS test
FROM base AS runtime
FROM scratch
COPY --from=build /out /out
FROM docker.io/library/nginx@sha256:f0e1d2c3b4a5968778695a4b3c2d1e0ff0e1d2c3b4a5968778695a4b3c2d1e0f AS static
FROM python:3.12-slim
FROM localhost:5000/team/tool:v1.2.3
FROM ${UNKNOWN_BASE} AS dropped
