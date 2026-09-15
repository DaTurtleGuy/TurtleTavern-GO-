package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

var deprecatedRedirects = [][2]string{
	{"/createcharacter", "/api/characters/create"},
	{"/renamecharacter", "/api/characters/rename"},
	{"/editcharacter", "/api/characters/edit"},
	{"/editcharacterattribute", "/api/characters/edit-attribute"},
	{"/v2/editcharacterattribute", "/api/characters/merge-attributes"},
	{"/deletecharacter", "/api/characters/delete"},
	{"/getcharacters", "/api/characters/all"},
	{"/getonecharacter", "/api/characters/get"},
	{"/getallchatsofcharacter", "/api/characters/chats"},
	{"/importcharacter", "/api/characters/import"},
	{"/dupecharacter", "/api/characters/duplicate"},
	{"/exportcharacter", "/api/characters/export"},
	{"/savechat", "/api/chats/save"},
	{"/getchat", "/api/chats/get"},
	{"/renamechat", "/api/chats/rename"},
	{"/delchat", "/api/chats/delete"},
	{"/exportchat", "/api/chats/export"},
	{"/importgroupchat", "/api/chats/group/import"},
	{"/importchat", "/api/chats/import"},
	{"/getgroupchat", "/api/chats/group/get"},
	{"/deletegroupchat", "/api/chats/group/delete"},
	{"/savegroupchat", "/api/chats/group/save"},
	{"/getgroups", "/api/groups/all"},
	{"/creategroup", "/api/groups/create"},
	{"/editgroup", "/api/groups/edit"},
	{"/deletegroup", "/api/groups/delete"},
	{"/getworldinfo", "/api/worldinfo/get"},
	{"/deleteworldinfo", "/api/worldinfo/delete"},
	{"/importworldinfo", "/api/worldinfo/import"},
	{"/editworldinfo", "/api/worldinfo/edit"},
	{"/getstats", "/api/stats/get"},
	{"/recreatestats", "/api/stats/recreate"},
	{"/updatestats", "/api/stats/update"},
	{"/getbackgrounds", "/api/backgrounds/all"},
	{"/delbackground", "/api/backgrounds/delete"},
	{"/renamebackground", "/api/backgrounds/rename"},
	{"/downloadbackground", "/api/backgrounds/upload"},
	{"/savetheme", "/api/themes/save"},
	{"/getuseravatars", "/api/avatars/get"},
	{"/deleteuseravatar", "/api/avatars/delete"},
	{"/uploaduseravatar", "/api/avatars/upload"},
	{"/deletequickreply", "/api/quick-replies/delete"},
	{"/savequickreply", "/api/quick-replies/save"},
	{"/uploadimage", "/api/images/upload"},
	{"/savemovingui", "/api/moving-ui/save"},
	{"/api/serpapi/search", "/api/search/serpapi"},
	{"/api/serpapi/visit", "/api/search/visit"},
	{"/api/serpapi/transcript", "/api/search/transcript"},
	{"/api/content/import", "/api/content/importURL"},
}

func RegisterDeprecatedRedirects(r chi.Router) {
	for _, redir := range deprecatedRedirects {
		src, dst := redir[0], redir[1]
		r.Handle(src, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			http.Redirect(w, req, dst, http.StatusPermanentRedirect)
		}))
	}
	r.Handle("/listimgfiles/{folder}", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		folder := chi.URLParam(req, "folder")
		http.Redirect(w, req, "/api/images/list/"+folder, http.StatusPermanentRedirect)
	}))
}
