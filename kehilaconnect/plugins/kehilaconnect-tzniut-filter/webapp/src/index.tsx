import React from 'react';

import ModeratorPanel from './components/ModeratorPanel';

const PLUGIN_ID = 'com.kehilaconnect.tzniut-filter';

class TzniutFilterPlugin {
    initialize(
        registry: {
            registerRightHandSidebarComponent: (
                component: React.ComponentType,
                title: string,
            ) => {id: string; showRHSAction: {type: string}};
        },
        _store: unknown,
    ): void {
        // Moderator dashboard accessible from the RHS — community moderators
        // open it to review and approve/reject flagged content.
        registry.registerRightHandSidebarComponent(ModeratorPanel, 'Content Moderation');
    }
}

declare global {
    interface Window {
        registerPlugin: (id: string, plugin: TzniutFilterPlugin) => void;
    }
}

window.registerPlugin(PLUGIN_ID, new TzniutFilterPlugin());
