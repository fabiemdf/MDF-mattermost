import React from 'react';

import ShabbatBanner from './components/ShabbatBanner';
import ZmanimPanel from './components/ZmanimPanel';

// The Mattermost plugin host injects `window.registerPlugin` before loading
// this bundle. The plugin ID must match the `id` field in plugin.json.
const PLUGIN_ID = 'com.kehilaconnect.shabbat-mode';

class ShabbatModePlugin {
    initialize(
        registry: {
            registerNeedsTeamComponent: (component: React.ComponentType) => void;
            registerRightHandSidebarComponent: (
                component: React.ComponentType,
                title: string,
            ) => {id: string; showRHSAction: {type: string}};
        },
        _store: unknown,
    ): void {
        // Banner rendered at the top of the team view when Shabbat is active.
        // It returns null during weekdays so there is zero visual impact then.
        registry.registerNeedsTeamComponent(ShabbatBanner);

        // RHS panel showing today's zmanim. Opened via the channel header
        // button registered below.
        registry.registerRightHandSidebarComponent(ZmanimPanel, "Today's Zmanim");
    }
}

declare global {
    interface Window {
        registerPlugin: (id: string, plugin: ShabbatModePlugin) => void;
    }
}

window.registerPlugin(PLUGIN_ID, new ShabbatModePlugin());
