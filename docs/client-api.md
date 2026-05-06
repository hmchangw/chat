# Chat Backend — Client API Reference

This document is the integrator-facing reference for the chat backend.
It covers every API a client (web, mobile, third-party) can call:

- **HTTP** — the single `POST /auth` endpoint that exchanges an SSO token
  for a NATS user JWT.
- **NATS request/reply** — RPC-style methods exposed by `room-service`,
  `history-service`, and `search-service`.
- **NATS publish + async reply** — the message-send flow handled by
  `message-gatekeeper`.

For each method, this doc lists the subject, the request body schema and
example, the success response schema and example, the error response, and
the server-pushed events the client will receive on the success and error
paths.

## Table of contents

1. [Overview](#1-overview)
2. [Connection & Auth](#2-connection--auth)
   - [2.1 NATS connection](#21-nats-connection)
   - [2.2 HTTP — POST /auth](#22-http--post-auth)
3. [Request/Reply Methods](#3-requestreply-methods)
   - [3.1 room-service](#31-room-service)
   - [3.2 history-service](#32-history-service)
   - [3.3 search-service](#33-search-service)
4. [Message Send](#4-message-send)
5. [Error envelope reference](#5-error-envelope-reference)

---

## 1. Overview

_(filled in by Task 2)_

---

## 2. Connection & Auth

### 2.1 NATS connection

_(filled in by Task 3)_

### 2.2 HTTP — POST /auth

_(filled in by Task 4)_

---

## 3. Request/Reply Methods

### 3.1 room-service

_(filled in by Tasks 6–13)_

### 3.2 history-service

_(filled in by Tasks 14–21)_

### 3.3 search-service

_(filled in by Tasks 22–23)_

---

## 4. Message Send

_(filled in by Task 24)_

---

## 5. Error envelope reference

_(filled in by Task 5)_
