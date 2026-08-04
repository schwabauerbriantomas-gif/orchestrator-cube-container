// SPDX-License-Identifier: Apache-2.0
// Copyright (C) 2026 Tencent. All rights reserved.

import enCommon from '@/locales/en/common.json';
import enNav from '@/locales/en/nav.json';
import enTopbar from '@/locales/en/topbar.json';
import enCommand from '@/locales/en/command.json';
import enOverview from '@/locales/en/overview.json';
import enSandboxes from '@/locales/en/sandboxes.json';
import enSandboxDetail from '@/locales/en/sandboxDetail.json';
import enTemplates from '@/locales/en/templates.json';
import enTemplateDetail from '@/locales/en/templateDetail.json';
import enNodes from '@/locales/en/nodes.json';
import enNodeDetail from '@/locales/en/nodeDetail.json';
import enVersions from '@/locales/en/versions.json';
import enNetwork from '@/locales/en/network.json';
import enKeys from '@/locales/en/keys.json';
import enPlaceholder from '@/locales/en/placeholder.json';
import enSandboxNew from '@/locales/en/sandboxNew.json';
import enTheme from '@/locales/en/theme.json';
import enSettings from '@/locales/en/settings.json';
import enObservability from '@/locales/en/observability.json';
import enStore from '@/locales/en/store.json';
import enAgentHub from '@/locales/en/agentHub.json';
import enAuth from '@/locales/en/auth.json';

export const resources = {
  en: {
    common: enCommon,
    nav: enNav,
    topbar: enTopbar,
    command: enCommand,
    overview: enOverview,
    sandboxes: enSandboxes,
    sandboxDetail: enSandboxDetail,
    templates: enTemplates,
    templateDetail: enTemplateDetail,
    nodes: enNodes,
    nodeDetail: enNodeDetail,
    versions: enVersions,
    network: enNetwork,
    keys: enKeys,
    placeholder: enPlaceholder,
    sandboxNew: enSandboxNew,
    theme: enTheme,
    settings: enSettings,
    observability: enObservability,
    store: enStore,
    agentHub: enAgentHub,
    auth: enAuth,
  },
} as const;

export type AppResources = typeof resources;
