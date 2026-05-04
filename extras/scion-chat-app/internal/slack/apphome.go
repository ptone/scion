// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package slack

import (
	"context"
	"fmt"

	slackapi "github.com/slack-go/slack"

	"github.com/GoogleCloudPlatform/scion/extras/scion-chat-app/internal/identity"
	"github.com/GoogleCloudPlatform/scion/extras/scion-chat-app/internal/state"
)

// publishAppHome builds and publishes the App Home tab for a user.
func (a *Adapter) publishAppHome(ctx context.Context, userID string) error {
	view := a.buildHomeView(ctx, userID)
	_, err := a.client.PublishViewContext(ctx, slackapi.PublishViewContextRequest{
		UserID: userID,
		View:   view,
	})
	if err != nil {
		a.log.Error("failed to publish app home", "user", userID, "error", err)
	}
	return err
}

// buildHomeView constructs the App Home tab view for the given user.
func (a *Adapter) buildHomeView(ctx context.Context, userID string) slackapi.HomeTabViewRequest {
	var blocks []slackapi.Block

	blocks = append(blocks,
		slackapi.NewHeaderBlock(slackapi.NewTextBlockObject("plain_text", "Scion", false, false)),
		slackapi.NewDividerBlock(),
	)

	// User profile section
	blocks = append(blocks, a.buildProfileSection(ctx, userID)...)
	blocks = append(blocks, slackapi.NewDividerBlock())

	// Linked groves section
	blocks = append(blocks, a.buildLinkedGrovesSection()...)
	blocks = append(blocks, slackapi.NewDividerBlock())

	// User subscriptions section
	blocks = append(blocks, a.buildSubscriptionsSection(userID)...)
	blocks = append(blocks, slackapi.NewDividerBlock())

	// Quick actions
	blocks = append(blocks, slackapi.NewSectionBlock(
		slackapi.NewTextBlockObject("mrkdwn", "*Quick Actions*", false, false),
		nil, nil,
	))
	blocks = append(blocks, slackapi.NewActionBlock("",
		slackapi.NewButtonBlockElement("home.help", "help",
			slackapi.NewTextBlockObject("plain_text", "Help", false, false),
		),
	))

	return slackapi.HomeTabViewRequest{
		Type: "home",
		Blocks: slackapi.Blocks{
			BlockSet: blocks,
		},
	}
}

func (a *Adapter) buildProfileSection(ctx context.Context, userID string) []slackapi.Block {
	var blocks []slackapi.Block
	blocks = append(blocks, slackapi.NewSectionBlock(
		slackapi.NewTextBlockObject("mrkdwn", "*Your Profile*", false, false),
		nil, nil,
	))

	if a.idMapper == nil {
		blocks = append(blocks, slackapi.NewSectionBlock(
			slackapi.NewTextBlockObject("mrkdwn", "Registration: _unavailable_", false, false),
			nil, nil,
		))
		return blocks
	}

	mapping, err := a.idMapper.Resolve(userID, PlatformName)
	if err != nil || mapping == nil {
		blocks = append(blocks, slackapi.NewSectionBlock(nil,
			[]*slackapi.TextBlockObject{
				slackapi.NewTextBlockObject("mrkdwn", "*Registration*", false, false),
				slackapi.NewTextBlockObject("mrkdwn", "Not registered", false, false),
			}, nil,
		))
		return blocks
	}

	blocks = append(blocks, slackapi.NewSectionBlock(nil,
		[]*slackapi.TextBlockObject{
			slackapi.NewTextBlockObject("mrkdwn", "*Registration*", false, false),
			slackapi.NewTextBlockObject("mrkdwn", "Registered", false, false),
		}, nil,
	))
	blocks = append(blocks, slackapi.NewSectionBlock(nil,
		[]*slackapi.TextBlockObject{
			slackapi.NewTextBlockObject("mrkdwn", "*Hub Email*", false, false),
			slackapi.NewTextBlockObject("mrkdwn", mapping.HubUserEmail, false, false),
		}, nil,
	))

	return blocks
}

func (a *Adapter) buildLinkedGrovesSection() []slackapi.Block {
	var blocks []slackapi.Block
	blocks = append(blocks, slackapi.NewSectionBlock(
		slackapi.NewTextBlockObject("mrkdwn", "*Linked Groves*", false, false),
		nil, nil,
	))

	if a.store == nil {
		return blocks
	}

	links, err := a.store.ListSpaceLinks()
	if err != nil {
		a.log.Error("failed to list space links for app home", "error", err)
		return blocks
	}

	var slackLinks []state.SpaceLink
	for _, link := range links {
		if link.Platform == PlatformName {
			slackLinks = append(slackLinks, link)
		}
	}

	if len(slackLinks) == 0 {
		blocks = append(blocks, slackapi.NewSectionBlock(
			slackapi.NewTextBlockObject("mrkdwn", "_No groves linked. Use `/scion link <grove-slug>` in a channel._", false, false),
			nil, nil,
		))
		return blocks
	}

	for _, link := range slackLinks {
		blocks = append(blocks, slackapi.NewSectionBlock(
			slackapi.NewTextBlockObject("mrkdwn",
				fmt.Sprintf("<#%s> → %s", link.SpaceID, link.GroveSlug),
				false, false,
			),
			nil, nil,
		))
	}

	return blocks
}

func (a *Adapter) buildSubscriptionsSection(userID string) []slackapi.Block {
	var blocks []slackapi.Block
	blocks = append(blocks, slackapi.NewSectionBlock(
		slackapi.NewTextBlockObject("mrkdwn", "*Your Subscriptions*", false, false),
		nil, nil,
	))

	if a.store == nil {
		return blocks
	}

	subs, err := a.store.ListUserSubscriptions(userID, PlatformName)
	if err != nil {
		a.log.Error("failed to list subscriptions for app home", "error", err)
		return blocks
	}

	if len(subs) == 0 {
		blocks = append(blocks, slackapi.NewSectionBlock(
			slackapi.NewTextBlockObject("mrkdwn", "_No subscriptions. Use `/scion subscribe <agent>` to subscribe._", false, false),
			nil, nil,
		))
		return blocks
	}

	for _, sub := range subs {
		activities := sub.Activities
		if activities == "" {
			activities = "all activities"
		}
		blocks = append(blocks, slackapi.NewSectionBlock(
			slackapi.NewTextBlockObject("mrkdwn",
				fmt.Sprintf("`%s` — %s", sub.AgentID, activities),
				false, false,
			),
			nil, nil,
		))
	}

	return blocks
}

// AppHomeStore provides the subset of state.Store needed by the App Home tab.
// Exposed separately so the adapter can be tested without a full store.
type AppHomeStore interface {
	ListSpaceLinks() ([]state.SpaceLink, error)
	ListUserSubscriptions(platformUserID, platform string) ([]state.AgentSubscription, error)
}

// AppHomeIdentity provides the subset of identity.Mapper needed by the App Home.
type AppHomeIdentity interface {
	Resolve(platformUserID, platform string) (*state.UserMapping, error)
}

// Ensure the real types satisfy these interfaces at compile time.
var _ AppHomeIdentity = (*identity.Mapper)(nil)
