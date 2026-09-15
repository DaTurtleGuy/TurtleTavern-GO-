import { eventSource, event_types } from './events.js';
import { is_group_generating, group_activation_strategy } from './group-chats.js';
import { getMessageTimeStamp } from './RossAscends-mods.js';
import {
    chat,
    addOneMessage,
    saveChatConditional
} from '../script.js';

const TURTLE_BUTTONS_ID = 'turtle_buttons';
const BUTTONS_CONTAINER_ID = 'buttons_container';
const TEMPLATE_SELECTOR = '#turtle_button_template .turtle_button';

let turtleResizeObserver = null;

function showTurtleButtons() {
    $(`#${TURTLE_BUTTONS_ID}`).show();
    eventSource.emit(event_types.TURTLE_BUTTONS_SHOWN);
}

export function hideTurtleButtons() {
    $(`#${TURTLE_BUTTONS_ID}`).hide();
    eventSource.emit(event_types.TURTLE_BUTTONS_HIDDEN);
}

export function populateTurtleButtons() {
    const value = parseInt($(`#rm_group_activation_strategy`).val().toString(), 10);

    if (value !== group_activation_strategy.TURTLE) {
        hideTurtleButtons();
        return;
    }

    console.log('Populating turtle buttons');
    showTurtleButtons();

    const members = $(`#rm_group_members .group_member`);
    const container = $(`#${BUTTONS_CONTAINER_ID}`);
    container.empty();

    const sortedMembers = members.toArray().sort((a, b) => {
        return parseInt($(a).css('order') || '0') - parseInt($(b).css('order') || '0');
    });

    $(sortedMembers).each(function () {
        const $member = $(this);
        const template = $(TEMPLATE_SELECTOR).clone();

        const name = $member.find('.ch_name').text();
        const speakButton = $member.find('.right_menu_button[data-action="speak"]');
        const avatarSrc = $member.find('.avatar img').attr('src');

        if (avatarSrc) {
            template.find('img').attr('src', avatarSrc);
        }

        template.find('.turtle_button_name').text(name);

        template.on('click', () => {
            if (is_group_generating) {
                return;
            }
            const text = $(`#send_textarea`).val().toString().trim();
            if (text) {
                addMessageFromCharacter(text, name, avatarSrc);
                $(`#send_textarea`).val('');
                setTimeout(() => {
                    $(`#send_textarea`).trigger('input');
                    $(`#send_textarea`).trigger('change');
                    $(`#send_textarea`).trigger('keyup');
                    $(`#send_textarea`).trigger('keydown');
                    $(`#send_textarea`).trigger('keypress');
                }, 100);
            } else {
                speakButton.trigger('click');
            }
        });

        container.append(template);
    });

    if (container.children().length > 0) {
        $(`#${TURTLE_BUTTONS_ID}`).show();
    } else {
        $(`#${TURTLE_BUTTONS_ID}`).hide();
        hideTurtleButtons();
    }
}

function addMessageFromCharacter(message, characterName, avatar) {
    const newMessage = {
        name: characterName,
        is_user: false,
        is_name: true,
        force_avatar: avatar,
        send_date: getMessageTimeStamp(),
        mes: message,
        extra: {},
    };

    chat.push(newMessage);
    addOneMessage(newMessage);
    saveChatConditional();
}

function adjustTurtleDimensions() {
    const topBarWidth = $(`#top-bar`).width();
    const turtleButtons = $(`#${TURTLE_BUTTONS_ID}`);
    const areButtonsVisible = $(`#${BUTTONS_CONTAINER_ID}`).is(':visible');

    if (!areButtonsVisible) {
        turtleButtons.width('auto');
    } else {
        turtleButtons.width(topBarWidth);
    }

    const leftPosition = (window.innerWidth - topBarWidth) / 2;
    turtleButtons.css({
        'left': leftPosition + 'px',
        'position': 'fixed',
    });
}

export function initTurtleButtons() {
    $(`#turtle_close`).on('click', function () {
        $(`#${BUTTONS_CONTAINER_ID}`).toggle();
        adjustTurtleDimensions();
    });

    $(window).on('resize', adjustTurtleDimensions);
    adjustTurtleDimensions();

    if (turtleResizeObserver) {
        turtleResizeObserver.disconnect();
    }

    turtleResizeObserver = new ResizeObserver(() => {
        adjustTurtleDimensions();
    });
    turtleResizeObserver.observe(document.body);

    eventSource.on(event_types.GROUP_UPDATED, populateTurtleButtons);
}
