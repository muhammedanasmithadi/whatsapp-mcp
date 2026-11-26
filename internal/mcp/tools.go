package mcp

func (s *Server) registerTools() {
	s.registerQueryTools()

	s.registerPreviewSend()

	s.registerResolveContacts()
	s.registerDiagnoseContacts()

	if s.readOnly {
		s.registerRequestSync()
		s.registerSyncContacts()
		s.registerResyncAppState()
		s.registerDownloadMedia()
		s.registerRecoverMedia()
		s.registerTranscribeVoiceNote()
		s.registerListGroups()
		s.registerGetGroupInfo()
		s.registerGetPollResults()
		s.registerGetBlocklist()
		s.registerGetPrivacySettings()
		return
	}

	s.registerSendTools()
	s.registerRejectCall()
	s.registerMessageTools()

	s.registerGroupTools()
	s.registerPrivacyTools()

	s.registerMediaTools()

	s.registerSocialActionTools()

	s.registerContactActionTools()
	s.registerSyncContacts()
	s.registerResyncAppState()
	s.registerSchedulingActionTools()

	s.registerChannelActionTools()
	s.registerChatStateTools()
}
