This is feedback on

.design/chat-plugin-tradeoffs.md

Gap 1: space routing. Would t the chat app be able to keep the mapping of grove to chat space/channel?

Gap 2: isn’t the impersonated user the sender? We would have a way to represent in the expected format for the sender

Gap 4. This would be outside the plugin interface scope and part of what makes the chat app a superset of capabilities

Regarding “PublishWithContext” there is no strong reason the chat app can’t call the job API to get required enrichment metadata, if that is something that is outside the needs of a typical message broker plugin

Strongly agree with : “Merge notification delivery into the plugin path”

Option C here was the original intent: “Option C: Single process — the plugin IS the app.” It would need to be run outside the plugin manager lifecycle. Which the plugin manager should support

We should pull in this optional suggestion
“Plugin-initiated subscriptions” as it seems rather important for the over all chat app requirements

Open question responses

1. We may need a different lifecycle mechanism for processes that offer such hybrid superset of capabilities. We are not trying to expose the superset as significant expansion of the message broker plugin API
2. Not an issue when plugin and chat app are one binary. Can persist this in SQLite as needed
3. Yes. As noted this will be single binary that acts as plugin+chat app

Based on this feedback revise the initial analysis. Then draft a .design/message-broker-plugin-evolution.md doc that focuses  on evolving the core plugin machinery without explicit concern of the chat app superset