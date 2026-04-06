chat app integration for scion
We want to set up a chat app integration design. It should support a common base architecture. With a first concrete implementation for Google chat. And a second target for Slack

Will act as a combination scion message broker plugin, and API proxy client for additional operations on agents in scion

Should import and use hub client library code from scion package but run as a standalone process. 

Written in golang, using

https://pkg.go.dev/cloud.google.com/go/chat/apiv1

Will be set up to be one chat Application per hub

Will typically use the hub’s operating service account

Will use chat cards to represent different communications types from hub-> chat

Will support @ mentions and slash commands for communicating with agents, as well as interacting with API (supporting a subset of things one would do with CLI. Supporting identical sub command syntax)

Will use dialogs as appropriate when command need input. Or for ask_user status from agents

Will keep a map of chat users to hub users and be able to impersonate users by making authorized requests to the API (will need access to hub signing keys)

A chat user can “link” a chat space to a grove through a slash command (user needs to be grove admin or owner)

The MVP should support basic messaging and some core operational commands to start, stop, and delete agents

Remember that we will build this as a core chat-app interface

Here are some notes about the multi provider design

# Google Chat to Slack: Architecture & Porting Guide

## 1. Component Analogs
| Feature | Google Chat (GCP) | Slack | Porting Notes |
| :--- | :--- | :--- | :--- |
| **Identity** | Service Account (JSON) | Bot Token (`xoxb-`) | Slack uses OAuth tokens; GCP uses IAM-based keys. |
| **UI Framework** | Google Card V1/V2 | Block Kit | Both use JSON. Slack is more modular; Google is more structured. |
| **Interactive UI** | Dialogs | Modals | High parity. Both support stacks and stateful updates. |
| **Discovery** | Slash Commands | Slash Commands | Identical registration logic (Command + URL). |
| **Messaging** | `@mention + argumentText` | `app_mention` Event | Both provide a raw string of text following the mention. |
| **Persistence** | Home Tab | App Home | Slack's Home Tab is more robust for dashboards. |

---

## 2. Multi-Agent Implementation Strategies
### Google Chat (Identity is Static)
*   **Method:** One GCP Project per agent **OR** use Card Headers to fake identity.
*   **Constraint:** The bot name/avatar in the message gutter is fixed by the GCP Console.
*   **Workaround:** Use the `CardHeader` widget to display "Agent: [Name]" and a custom icon.

### Slack (Identity is Dynamic)
*   **Method:** Overwrite identity per-request.
*   **Advantage:** A single bot token can post as any name/avatar.
*   **Go Snippet:** 
    ```go
    slack.MsgOptionUsername("Neuro-Analyst"),
    slack.MsgOptionIconURL("[https://cdn.com/brain.png](https://cdn.com/brain.png)")
    ```

---

## 3. Technical Porting Tips (Golang)

### Abstraction Layer
Define a `Messenger` interface in Go to decouple your business logic (the "Agent" processing) from the platform-specific API calls:
```go
type Messenger interface {
    SendMessage(ctx context.Context, channelID string, text string, blocks interface{}) error
    OpenUI(ctx context.Context, triggerID string, view interface{}) error
}


Write an initial draft design doc to

.design/google-chat.md

We may need addition breakout docs to cover parts of this in a dedicated treatment. 