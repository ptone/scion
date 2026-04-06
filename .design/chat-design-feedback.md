
The /scion register action should short circuit if a matching email is found

Clarify that the chat app operates under different identities scenarios . 
A hub user, usually as admin, for “system level “ hub operations
a hub operational environment account, typically the same GCP service account the hub server runs as, for accessing things like the GCP secret manager, and for logging. Finally it operates on HUB APIs impersonating chat users that are linked to hub users

For Threads. The “Scion message” command will need a —thread flag to support agents being able to reply to a thread. The help text for this should clarify it is only used when replying to a message that contains a thread ID. And this will need to become part of the formatted message type. This could be split into its one implementation phase, or a sub design doc. 

Regarding open question 1 on threading. It is assumed that both gChat and slack have an implicit thread ID generated for every message posted to space/channel without a thread identified (new thread, root message). If so we should just assume threading and make a go of it. Refine later if needed

For remaining open question 2, if the dialog format supports a set of checkboxes. We should allow for the user to choose