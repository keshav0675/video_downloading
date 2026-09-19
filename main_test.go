package main

import "testing"

func TestSharedVideoLinks(t *testing.T) {
	tiktok := "https://www.tiktok.com/@.zek2/video/7674128746114190613?is_from_webapp=1&sender_device=pc"
	shorts := "https://youtube.com/shorts/Y_yw1GkeDqc?si=lpQAmdH0vxrNYxAw"
	for _, link := range []string{tiktok, "https://vm.tiktok.com/ZMabcdef/?share=1", "https://vt.tiktok.com/ZMabcdef/"} {
		if !isJustLink(link, tiktokRegex) || extractLink("  "+link+"\n") != link {
			t.Errorf("TikTok share link was rejected or truncated: %s", link)
		}
	}
	for _, link := range []string{shorts, "https://m.youtube.com/shorts/Y_yw1GkeDqc/"} {
		if !isJustLink(link, youtubeRegex) || extractLink("  "+link+"\n") != link {
			t.Errorf("Shorts share link was rejected or truncated: %s", link)
		}
	}
	if youtubeRegex.MatchString("https://youtube.com/shorts/Y_yw1GkeDqcEXTRA") {
		t.Error("accepted malformed YouTube ID")
	}
}
