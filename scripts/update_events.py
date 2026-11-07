#!/usr/bin/env python3
"""Replace the auto-responder block in events.go with the new version."""
with open("internal/client/events.go", "r") as f:
    lines = f.readlines()

new_lines = [
    "\t\t\t// Auto-responder: reply to incoming 1:1 text messages when enabled.\n",
    "\t\t\t// Runs in a goroutine so a slow or rate-limited send never blocks\n",
    "\t\t\t// message handling. WhatsApp push delivers the incoming message, so\n",
    "\t\t\t// this is event-driven — no polling.\n",
    "\t\t\tchatJID := nm.chatJID\n",
    "\t\t\tcontent := nm.msg.Content\n",
    "\t\t\tisFromMe := msg.Info.IsFromMe\n",
    "\t\t\tgo func() {\n",
    "\t\t\t\tcan, err := c.store.CanAutoRespond(ctx, chatJID)\n",
    "\t\t\t\tif err != nil {\n",
    "\t\t\t\t\tc.log.Warnf(\"auto-responder: config check failed: %v\", err)\n",
    "\t\t\t\t\treturn\n",
    "\t\t\t\t}\n",
    "\t\t\t\tif !can {\n",
    "\t\t\t\t\treturn\n",
    "\t\t\t\t}\n",
    "\t\t\t\tif isFromMe {\n",
    "\t\t\t\t\treturn\n",
    "\t\t\t\t}\n",
    "\t\t\t\tif strings.HasSuffix(chatJID, \"@g.us\") || strings.HasSuffix(chatJID, \"@newsletter\") {\n",
    "\t\t\t\t\treturn\n",
    "\t\t\t\t}\n",
    "\t\t\t\tif chatJID == store.StatusBroadcastJID {\n",
    "\t\t\t\t\treturn\n",
    "\t\t\t\t}\n",
    "\t\t\t\tif strings.TrimSpace(content) == \"\" {\n",
    "\t\t\t\t\treturn\n",
    "\t\t\t\t}\n",
    "\t\t\t\t// Get the reply message\n",
    "\t\t\t\t_, reply, _, _, _, err := c.store.GetAutoResponderWithConfig(ctx)\n",
    "\t\t\t\tif err != nil {\n",
    "\t\t\t\t\tc.log.Warnf(\"auto-responder: config check failed: %v\", err)\n",
    "\t\t\t\t\treturn\n",
    "\t\t\t\t}\n",
    "\t\t\t\tif reply == \"\" {\n",
    "\t\t\t\t\treturn\n",
    "\t\t\t\t}\n",
    "\t\t\t\tr := c.Send(context.Background(), chatJID, reply)\n",
    "\t\t\t\tif !r.Success {\n",
    "\t\t\t\t\tc.log.Warnf(\"auto-responder: failed to reply to %s: %s\",\n",
    "\t\t\t\t\t\tc.redactor.JID(chatJID), r.Message)\n",
    "\t\t\t\t\treturn\n",
    "\t\t\t\t}\n",
    "\t\t\t\tif err := c.store.RecordAutoRespond(ctx, chatJID); err != nil {\n",
    "\t\t\t\t\tc.log.Warnf(\"auto-responder: failed to record reply time: %v\", err)\n",
    "\t\t\t\t}\n",
    "\t\t\t\tc.log.Infof(\"auto-responder: replied to %s\", c.redactor.JID(chatJID))\n",
    "\t\t\t}()\n",
]

# Replace lines 272-306 (0-indexed: 271-305 inclusive)
new_lines_list = lines[:271] + new_lines + lines[306:]
with open("internal/client/events.go", "w") as f:
    f.writelines(new_lines_list)
print("Done")
