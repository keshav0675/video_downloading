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

func TestFacebookSharedVideoLinks(t *testing.T) {
	for _, link := range []string{
		"https://www.facebook.com/100068326152285/videos/1438457594815733/?__so__=discover&__rv__=video_home_www_loe_popular_videos",
		"https://www.facebook.com/share/v/AbCd123/?mibextid=test",
		"https://www.facebook.com/share/r/AbCd123/",
		"https://m.facebook.com/reel/123/?s=single_unit",
		"https://www.facebook.com/watch/?v=123&ref=sharing",
		"https://facebook.com/watch?v=123",
		"https://www.facebook.com/video.php?v=123&ref=sharing",
		"https://fb.watch/AbCd_123-/?mibextid=test",
	} {
		if !isJustLink(link, facebookRegex) || extractLink("  "+link+"\n") != link {
			t.Errorf("Facebook share link was rejected or truncated: %s", link)
		}
	}
	for _, link := range []string{
		"https://facebook.com.evil.test/reel/123/",
		"https://www.facebook.com/reel/123extra",
		"https://www.facebook.com/share/v/AbCd123/extra",
		"https://www.facebook.com/reel/123/ another message",
	} {
		if facebookRegex.MatchString(link) {
			t.Errorf("accepted invalid Facebook link: %s", link)
		}
	}
}
