package gate

import "testing"

func TestEligible_SameRepoMechanuAuthor(t *testing.T) {
	pr := PRContext{IsCrossRepository: false, Author: "mechanu[bot]"}
	if !Eligible(pr) {
		t.Fatal("Eligible() = false, want true for same-repo mechanu[bot]-authored PR")
	}
}

func TestEligible_ForkPR_Ineligible(t *testing.T) {
	pr := PRContext{IsCrossRepository: true, Author: "mechanu[bot]"}
	if Eligible(pr) {
		t.Fatal("Eligible() = true, want false for a fork PR even when author claims mechanu[bot]")
	}
}

func TestEligible_SameRepoHumanAuthor_Ineligible(t *testing.T) {
	pr := PRContext{IsCrossRepository: false, Author: "chnu-kim"}
	if Eligible(pr) {
		t.Fatal("Eligible() = true, want false for a same-repo PR authored by a human/write-access account")
	}
}

func TestEligible_SameRepoOtherBotAuthor_Ineligible(t *testing.T) {
	pr := PRContext{IsCrossRepository: false, Author: "dependabot[bot]"}
	if Eligible(pr) {
		t.Fatal("Eligible() = true, want false for a same-repo PR authored by a different bot")
	}
}

func TestEligible_ForkAndWrongAuthor_Ineligible(t *testing.T) {
	pr := PRContext{IsCrossRepository: true, Author: "attacker"}
	if Eligible(pr) {
		t.Fatal("Eligible() = true, want false when both conditions fail")
	}
}
